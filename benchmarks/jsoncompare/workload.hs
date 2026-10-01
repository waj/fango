{-# LANGUAGE DeriveAnyClass #-}
{-# LANGUAGE DeriveGeneric #-}
{-# LANGUAGE StrictData #-}

-- The same typed, retained order array as workload.fango and controls.txt.
module Main (main) where

import Control.DeepSeq (NFData, force)
import Control.Exception (bracket, evaluate)
import Data.Aeson (FromJSON (parseJSON), Options (fieldLabelModifier), defaultOptions, eitherDecodeStrict, genericParseJSON)
import qualified Data.ByteString as Bytes
import Data.Int (Int64)
import Data.Text (Text)
import Data.Text.Foreign (lengthWord8)
import Foreign.StablePtr (freeStablePtr, newStablePtr)
import GHC.Generics (Generic)
import System.Environment (getArgs)
import System.Exit (die)

data Shipping = Shipping
    { s_city :: Text
    , s_postal :: Text
    , s_latitude :: Double
    , s_longitude :: Double
    } deriving (Generic, NFData)

data Item = Item
    { i_sku :: Text
    , i_quantity :: Int64
    , i_unit_cents :: Int64
    } deriving (Generic, NFData)

data Order = Order
    { o_id :: Int64
    , o_customer :: Text
    , o_active :: Bool
    , o_total_cents :: Int64
    , o_discount :: Maybe Text
    , o_shipping :: Shipping
    , o_tags :: [Text]
    , o_items :: [Item]
    , o_description :: Text
    } deriving (Generic, NFData)

-- Strip each type's two-character field prefix to match the fixture keys.
jsonOptions :: Options
jsonOptions = defaultOptions { fieldLabelModifier = drop 2 }

instance FromJSON Shipping where
    parseJSON = genericParseJSON jsonOptions

instance FromJSON Item where
    parseJSON = genericParseJSON jsonOptions

instance FromJSON Order where
    parseJSON = genericParseJSON jsonOptions

data Totals = Totals Int64 Int64 Int64 Int64 Int64

summarize :: Totals -> Order -> Totals
summarize (Totals records ids cents units textBytes) order = Totals
    (records + 1)
    (ids + o_id order)
    (cents + o_total_cents order)
    (units + foldl' (\n item -> n + i_quantity item) 0 (o_items order))
    (textBytes + fromIntegral (lengthWord8 (o_description order)))

run :: FilePath -> IO ()
run path = do
    input <- Bytes.readFile path
    case eitherDecodeStrict input :: Either String [Order] of
        Left message -> die message
        Right decoded -> do
            -- Force every field, including fields unused by the checksum.
            -- Keep the complete typed result alive until verification ends.
            orders <- evaluate (force decoded)
            bracket (newStablePtr orders) freeStablePtr $ \_ -> do
                Totals records ids cents units textBytes <- evaluate
                    (foldl' summarize (Totals 0 0 0 0 0) orders)
                putStrLn (unwords (map show [records, ids, cents, units, textBytes]))

main :: IO ()
main = do
    args <- getArgs
    case args of
        [path] -> run path
        _ -> die "usage: haskell-json FILE"
